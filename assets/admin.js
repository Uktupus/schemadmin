// Progressive enhancement only. Without this file every link is a page and
// every form a normal submission; with it, rows open in a dialog.
(function () {
  "use strict";

  var MODAL_HEADER = "X-Schemadmin-Modal";
  var dialog = document.getElementById("modal");
  var modalBody = dialog && dialog.querySelector("[data-modal-body]");
  var canModal = !!(dialog && dialog.showModal && window.fetch && window.URLSearchParams);

  // --- Dialog ---------------------------------------------------------

  // show loads a page of the admin, without its frame, into the dialog.
  function show(url, options) {
    options = options || {};
    options.credentials = "same-origin";
    options.headers = {};
    options.headers[MODAL_HEADER] = "1";
    var posting = options.method === "POST";
    dialog.setAttribute("aria-busy", "true");
    return fetch(url, options)
      .then(function (res) {
        // A write that succeeded answers with a redirect.
        if (posting && res.redirected) {
          finish(res.url);
          return null;
        }
        return res.text();
      })
      .then(function (html) {
        if (html === null) return;
        modalBody.innerHTML = html;
        if (!dialog.open) dialog.showModal();
        modalBody.scrollTop = 0;
        var heading = modalBody.querySelector("h1");
        if (heading) dialog.setAttribute("aria-label", heading.textContent);
        var bad = modalBody.querySelector(".bad input, .bad select, .bad textarea");
        if (bad) bad.focus();
      })
      .catch(function () {
        // Fall back to the page itself.
        if (!posting) window.location.href = url;
      })
      .then(function () {
        dialog.removeAttribute("aria-busy");
      });
  }

  // finish closes the dialog after a write and refreshes what is behind
  // it, carrying the outcome ("saved", "created", "deleted") along.
  function finish(redirectedTo) {
    var outcome = new URL(redirectedTo, window.location.href).searchParams.get("m");
    if (outcome === "nochange") {
      dialog.close();
      return;
    }
    // Behind the dialog is either a list, which is reloaded as it was,
    // or the page of the row itself, which may no longer exist.
    if (!document.querySelector("[data-list]")) {
      window.location.replace(redirectedTo);
      return;
    }
    var here = new URL(window.location.href);
    if (outcome) here.searchParams.set("m", outcome);
    window.location.replace(here.toString());
  }

  function plainClick(event) {
    return event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey;
  }

  document.addEventListener("click", function (event) {
    if (!plainClick(event)) return;
    var target = event.target;
    var link = target.closest && target.closest("a");

    if (canModal) {
      // Close buttons and "Cancel" close the dialog when inside one.
      var closer = target.closest("[data-close]");
      if (closer && dialog.open && dialog.contains(closer)) {
        event.preventDefault();
        dialog.close();
        return;
      }
      // A click on the backdrop.
      if (target === dialog) {
        dialog.close();
        return;
      }
      if (link && link.hasAttribute("data-modal")) {
        event.preventDefault();
        show(link.href);
        return;
      }
    }

    // Rows are clickable as a whole.
    if (link || target.closest("button, input, select, textarea, label")) return;
    if (String(window.getSelection && window.getSelection()) !== "") return;
    var row = target.closest("tr[data-row], tr[data-href]");
    if (!row) return;
    if (row.hasAttribute("data-row") && canModal) {
      show(row.getAttribute("data-row"));
    } else {
      window.location.href = row.getAttribute("data-row") || row.getAttribute("data-href");
    }
  });

  if (canModal) {
    dialog.addEventListener("submit", function (event) {
      var form = event.target;
      if ((form.getAttribute("method") || "").toLowerCase() !== "post") return;
      event.preventDefault();
      show(form.action, { method: "POST", body: new URLSearchParams(new FormData(form)) });
    });
    dialog.addEventListener("close", function () {
      modalBody.textContent = "";
    });
  }

  // --- Column menus -----------------------------------------------------

  // Popovers open in the middle of the screen by default; put each one
  // under the header that opened it.
  document.addEventListener("toggle", function (event) {
    var menu = event.target;
    if (!menu.classList || !menu.classList.contains("menu") || event.newState !== "open") return;
    var opener = document.querySelector('[popovertarget="' + menu.id + '"]');
    if (!opener) return;
    var box = opener.getBoundingClientRect();
    var width = menu.offsetWidth;
    var left = Math.min(Math.max(8, box.left), window.innerWidth - width - 8);
    var top = box.bottom + 4;
    if (top + menu.offsetHeight > window.innerHeight - 8) {
      top = Math.max(8, window.innerHeight - menu.offsetHeight - 8);
    }
    menu.style.inset = "auto";
    menu.style.margin = "0";
    menu.style.left = left + "px";
    menu.style.top = top + "px";
    var first = menu.querySelector("input:not([type=hidden])");
    if (first) first.focus();
  }, true);

  // --- Sidebar ------------------------------------------------------------

  var navFilter = document.querySelector("[data-nav-filter]");
  if (navFilter) {
    navFilter.addEventListener("input", function () {
      var needle = navFilter.value.trim().toLowerCase();
      document.querySelectorAll(".side nav li").forEach(function (li) {
        li.hidden = needle !== "" && li.textContent.toLowerCase().indexOf(needle) === -1;
      });
    });
  }

  // --- Forms (delegated, so they work inside the dialog too) --------------

  // NULL boxes: ticking one greys the field out; typing in the field
  // unticks it. The server decides either way.
  document.addEventListener("change", function (event) {
    var box = event.target;
    if (!box.matches || !box.matches("input[data-null-for]")) return;
    var field = document.getElementById(box.getAttribute("data-null-for"));
    if (field) field.classList.toggle("is-null", box.checked);
  });

  function untick(event) {
    var field = event.target;
    if (!field.id || !field.name || field.value === "") return;
    var box = document.querySelector('input[data-null-for="' + field.id + '"]');
    if (box && box.checked) {
      box.checked = false;
      field.classList.remove("is-null");
    }
  }
  document.addEventListener("input", untick);
  document.addEventListener("change", untick);

  // Foreign keys with too many targets for a select: suggest while typing.
  var timer = null;
  var latest = 0;
  document.addEventListener("input", function (event) {
    var input = event.target;
    if (!input.matches || !input.matches("input[data-options]") || !window.fetch) return;
    var list = document.getElementById(input.getAttribute("list"));
    if (!list) return;
    clearTimeout(timer);
    timer = setTimeout(function () {
      var ticket = ++latest;
      var url = input.getAttribute("data-options") + "&q=" + encodeURIComponent(input.value);
      fetch(url, { credentials: "same-origin", headers: { Accept: "application/json" } })
        .then(function (res) { return res.ok ? res.json() : []; })
        .then(function (items) {
          if (ticket !== latest) return;
          list.textContent = "";
          items.forEach(function (item) {
            var option = document.createElement("option");
            option.value = item.value;
            option.label = item.label;
            option.textContent = item.label;
            list.appendChild(option);
          });
        })
        .catch(function () {});
    }, 180);
  });
})();
