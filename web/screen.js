(function () {
  'use strict';
  var layers = document.querySelectorAll('.layer');
  var idle = document.getElementById('idle');
  var logo = idle.querySelector('img');
  var wordmark = idle.querySelector('span');
  var items = [], settings = { duration: 15, transition: 'fade', transitionMs: 1000 };
  var failed = Object.create(null);
  var current = '', front = 0, playing = false, timer, pending, preloaded;
  var supportsEffects = typeof CSS !== 'undefined' && CSS.supports && CSS.supports('transform', 'translateX(1px)');
  function preload(url) {
    var image = new Image(), result = { url: url, image: image };
    result.promise = new Promise(function (resolve) {
      var deadline = setTimeout(function () { resolve(false); }, 10000);
      image.onload = function () { clearTimeout(deadline); resolve(true); };
      image.onerror = function () { clearTimeout(deadline); resolve(false); };
      image.src = url;
    });
    return result;
  }
  function nextItem() {
    var index = -1, now = Date.now();
    for (var i = 0; i < items.length; i++) {
      if (items[i].url === current) { index = i; break; }
    }
    for (var n = 1; n <= items.length; n++) {
      var candidate = items[(index + n) % items.length];
      if (!failed[candidate.url] || failed[candidate.url] <= now) return candidate;
    }
  }
  function queue(ms) { clearTimeout(timer); timer = setTimeout(advance, ms); }
  function settle(ms) {
    playing = false;
    var following = nextItem();
    preloaded = following && following.url !== current ? preload(following.url) : null;
    queue(ms);
  }
  function advance() {
    if (playing) return;
    var next = nextItem();
    var out = layers[front];
    if (!next) {
      if (items.length) { queue(3000); return; }
      current = '';
      preloaded = null;
      out.className = 'layer';
      idle.classList.add('visible');
      return;
    }
    if (current === next.url) { queue(settings.duration * 1000); return; }
    playing = true;
    var ready = preloaded && preloaded.url === next.url ? preloaded : preload(next.url);
    ready.promise.then(function (loaded) {
      if (!loaded) failed[next.url] = Date.now() + 15000;
      if (!loaded || !items.some(function (item) { return item.url === next.url; })) { settle(3000); return; }
      delete failed[next.url];
      var effect = settings.transition;
      if (effect !== 'none' && (settings.fallback || !supportsEffects)) effect = 'fade';
      var ms = effect === 'none' ? 0 : settings.transitionMs;
      var hadSlide = current !== '';
      var back = layers[1 - front];
      var incoming = back.querySelector('img');
      if (ready.image !== incoming) {
        ready.image.alt = '';
        back.replaceChild(ready.image, incoming);
        incoming = ready.image;
      }
      back.style.zIndex = '2';
      out.style.zIndex = '1';
      back.style.transitionDuration = '0ms';
      out.style.transitionDuration = (effect === 'black' ? ms / 2 : ms) + 'ms';
      idle.style.zIndex = '3';
      idle.style.transitionDuration = ms + 'ms';
      incoming.style.transitionDuration = (settings.duration * 1000 + ms) + 'ms';
      back.className = 'layer' + (effect === 'slide' && hadSlide ? ' slide-next' : '') + (effect === 'zoom' ? ' zoom' : '');
      void back.offsetWidth;
      function show(duration) {
        back.style.transitionDuration = duration + 'ms';
        back.classList.remove('slide-next');
        back.classList.add('visible');
        idle.classList.remove('visible');
        current = next.url;
        front = 1 - front;
        setTimeout(function () {
          out.className = 'layer';
          out.querySelector('img').removeAttribute('src');
          if (!items.length) { playing = false; advance(); return; }
          settle(settings.duration * 1000);
        }, duration);
      }
      if (hadSlide && effect === 'black') {
        out.classList.remove('visible');
        setTimeout(function () { show(ms / 2); }, ms / 2);
      } else {
        if (effect === 'slide' && hadSlide) out.classList.add('slide-out');
        show(ms);
      }
    });
  }
  function configure(data) {
    items = data.slides;
    Object.keys(failed).forEach(function (url) {
      if (!items.some(function (item) { return item.url === url; })) delete failed[url];
    });
    settings = data.settings;
    var url = settings.logo || '';
    if (logo.getAttribute('src') !== (url || null)) {
      logo.hidden = true;
      wordmark.hidden = false;
      if (url) logo.setAttribute('src', url); else logo.removeAttribute('src');
    }
    if (!playing && (!current || !items.length)) { clearTimeout(timer); advance(); }
  }
  logo.onload = function () { logo.hidden = false; wordmark.hidden = true; };
  logo.onerror = function () { logo.hidden = true; wordmark.hidden = false; };
  function poll() {
    if (pending && pending.readyState !== 4) pending.abort();
    var req = new XMLHttpRequest();
    pending = req;
    req.open('GET', '/api/playlist', true);
    req.timeout = 15000;
    req.onload = function () {
      if (req.status !== 200) return;
      try { configure(JSON.parse(req.responseText)); } catch (_) {}
    };
    req.send();
  }
  poll();
  setInterval(poll, 60000);
}());
