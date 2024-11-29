from flask import Flask, render_template, request, make_response, g
import redis
import os
import socket
import random
import json
import time
import logging

option_a = os.getenv("OPTION_A", "Glaces")
option_b = os.getenv("OPTION_B", "Sorbets")
redis_connection_string = os.environ["REDIS_CONNECTION_STRING"]
hostname = socket.gethostname()

app = Flask(__name__)

gunicorn_error_logger = logging.getLogger("gunicorn.error")
app.logger.handlers.extend(gunicorn_error_logger.handlers)
app.logger.setLevel(logging.ERROR)


def get_redis():
    if not hasattr(g, "redis"):
        max_retries = 5
        retry_delay = 1

        for attempt in range(max_retries):
            try:
                g.redis = redis.from_url(redis_connection_string)
                g.redis.ping()
                break
            except ConnectionError as e:
                if attempt == max_retries - 1:
                    raise
                app.logger.warning(
                    f"Redis connection attempt {attempt + 1} failed, retrying in {retry_delay}s"
                )
                time.sleep(retry_delay)
                retry_delay *= 2  # Exponential backoff

    return g.redis


@app.route("/", methods=["POST", "GET"])
def hello():
    voter_id = request.cookies.get("voter_id")
    if not voter_id:
        voter_id = hex(random.getrandbits(64))[2:-1]

    vote = None

    if request.method == "POST":
        redis = get_redis()
        vote = request.form["vote"]
        app.logger.info("Received vote for %s", vote)
        data = json.dumps({"voter_id": voter_id, "vote": vote})
        redis.rpush("votes", data)

    resp = make_response(
        render_template(
            "index.html",
            option_a=option_a,
            option_b=option_b,
            hostname=hostname,
            vote=vote,
        )
    )
    resp.set_cookie("voter_id", voter_id)
    return resp
