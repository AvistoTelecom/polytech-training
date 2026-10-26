var bg1 = document.getElementById('background-stats-1');
var bg2 = document.getElementById('background-stats-2');
var aPercent = document.getElementById('a-percent');
var bPercent = document.getElementById('b-percent');
var total = document.getElementById('total');

var scores = new EventSource('scores');

scores.addEventListener('open', function () {
  document.body.style.opacity = 1;
});

scores.addEventListener('scores', function (e) {
  var data = JSON.parse(e.data);
  var a = parseInt(data.a || 0);
  var b = parseInt(data.b || 0);

  var percentages = getPercentages(a, b);

  bg1.style.width = percentages.a + '%';
  bg2.style.width = percentages.b + '%';

  aPercent.textContent = percentages.a.toFixed(1);
  bPercent.textContent = percentages.b.toFixed(1);
  total.textContent = formatTotal(a + b);
});

scores.addEventListener('dberror', showError);
scores.addEventListener('error', showError);

function showError() {
  total.textContent = 'Database connection error';
}

function formatTotal(n) {
  if (n === 0) return 'No votes yet';
  if (n === 1) return '1 vote';
  return n + ' votes';
}

function getPercentages(a, b) {
  var result = {};

  if (a + b > 0) {
    result.a = Math.round(a / (a + b) * 100);
    result.b = 100 - result.a;
  } else {
    result.a = result.b = 50;
  }

  return result;
}
