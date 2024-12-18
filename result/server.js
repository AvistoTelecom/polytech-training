const { exit } = require('process');

var express = require('express'),
    async = require('async'),
    { Pool } = require('pg'),
    cookieParser = require('cookie-parser'),
    path = require('path'), // Added 'path' for resolving paths
    app = express(),
    server = require('http').Server(app),
    io = require('socket.io')(server);

var port = process.env.PORT || 4000;

io.on('connection', function (socket) {
  socket.emit('message', { text: 'Welcome!' });

  socket.on('subscribe', function (data) {
    socket.join(data.channel);
  });
});

if (process.env.POSTGRESQL_CONNECTION_STRING) {
  var connectionString = process.env.POSTGRESQL_CONNECTION_STRING;
} else {
  console.error('POSTGRESQL_CONNECTION_STRING env var is empty.\nExiting.');
  exit(1);
}

var pool = new Pool({
  connectionString
});

// Retry connecting to the database
async.retry(
  { times: 3, interval: 1000 },
  function (callback) {
    pool.connect(function (err, client, done) {
      if (err) {
        console.error(err);
        console.error("Waiting for db...");
      }
      callback(err, client); // Pass client if successful
    });
  },
  function (err, client) {
    if (err) {
      console.error("Giving up. Could not connect to database.");
      exit(1); // Exit if database connection fails
    }
    console.log("Connected to db");

    // Start the server only after successful DB connection
    server.listen(port, function () {
      console.log('App running on port ' + port);
    });

    // Start querying the database
    getVotes(client);
  }
);

function getVotes(client) {
  client.query('SELECT vote, COUNT(id) AS count FROM votes GROUP BY vote', [], function (err, result) {
    if (err) {
      console.error("Error performing query: " + err);
    } else {
      var votes = collectVotesFromResult(result);
      io.sockets.emit("scores", JSON.stringify(votes));
    }

    setTimeout(function () { getVotes(client); }, 1000);
  });
}

function collectVotesFromResult(result) {
  var votes = { a: 0, b: 0 };

  result.rows.forEach(function (row) {
    votes[row.vote] = parseInt(row.count);
  });

  return votes;
}

app.use(cookieParser());
app.use(express.urlencoded({ extended: false }));
app.use(express.static(__dirname + '/views'));

app.get('/', function (req, res) {
  res.sendFile(path.resolve(__dirname + '/views/index.html'));
});
