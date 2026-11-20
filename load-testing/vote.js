// Each virtual user (VU) opens the vote page, votes at random, then waits one second.
// Usage: cat load-testing/vote.js | docker run --rm -i -e BASE_URL=<url> grafana/k6 run -
import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '30s', target: 50 },
    { duration: '2m', target: 50 },
    { duration: '30s', target: 0 },
  ],
};

const url = `${__ENV.BASE_URL}/vote/`;

export default function () {
  http.get(url);
  const vote = Math.random() < 0.5 ? 'a' : 'b';
  const res = http.post(url, { vote: vote });
  check(res, { 'vote accepté': (r) => r.status === 200 });
  sleep(1);
}
