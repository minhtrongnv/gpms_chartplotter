// Hide the startup splash once the chart plotter has painted its first frame.
// A safety timeout guarantees the UI is never permanently covered if "ready"
// is not emitted because startup failed part-way through.
(function () {
  var done = false;

  function hide() {
    if (done) return;
    done = true;

    var splash = document.getElementById("splash");
    if (!splash) return;

    splash.classList.add("hide");

    setTimeout(function () {
      splash.remove();
    }, 500);
  }

  addEventListener("ready", hide, { once: true });
  setTimeout(hide, 20000);
})();
