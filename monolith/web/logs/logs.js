// Terminal-style panel showing the application logs streamed from the server (SSE).
(function () {
  var maxLines = 500;

  var panel = document.createElement('div');
  panel.id = 'app-logs';
  panel.innerHTML = '<div class="bar"><span>application logs</span><span class="status">connecting...</span></div><pre></pre>';
  document.body.appendChild(panel);
  document.body.classList.add('logs-on');

  var pre = panel.querySelector('pre');
  var status = panel.querySelector('.status');
  var stream = new EventSource(document.currentScript.dataset.src);

  // The server replays its recent history on every (re)connection.
  stream.onopen = function () {
    pre.textContent = '';
    status.textContent = 'live';
  };

  stream.onerror = function () {
    status.textContent = 'disconnected, retrying...';
  };

  stream.onmessage = function (e) {
    var atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 5;
    var level = (/\blevel=(\w+)/.exec(e.data) || [])[1];
    var line = document.createElement('div');

    line.textContent = e.data;
    if (level) line.className = level;
    pre.appendChild(line);

    while (pre.childNodes.length > maxLines) pre.removeChild(pre.firstChild);
    if (atBottom) pre.scrollTop = pre.scrollHeight;
  };
})();
