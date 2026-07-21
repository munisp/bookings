/*!
 * OpenDesk embed loader — injects the booking + chat widget as an iframe.
 *
 * Usage:
 *   <script src="https://<your-opendesk-host>/embed.js" data-site="acme" async></script>
 *
 * Optional data attributes:
 *   data-site     (required) public site slug, e.g. "acme"
 *   data-height   iframe height, default "640px"
 *   data-width    iframe width,  default "100%"
 *   data-target   CSS selector of the element to mount into; default: the
 *                 script tag is replaced in place.
 *
 * The loader is dependency-free and lazy: it only creates the iframe once
 * the host page has finished parsing.
 */
(function () {
  "use strict";

  function mount(script) {
    var site = script.getAttribute("data-site");
    if (!site) {
      console.error("[opendesk-embed] missing data-site attribute");
      return;
    }
    var origin = new URL(script.src).origin;
    var iframe = document.createElement("iframe");
    iframe.src = origin + "/embed/" + encodeURIComponent(site);
    iframe.title = "Booking widget";
    iframe.style.border = "0";
    iframe.style.width = script.getAttribute("data-width") || "100%";
    iframe.style.height = script.getAttribute("data-height") || "640px";
    iframe.style.maxWidth = "100%";
    iframe.setAttribute("loading", "lazy");
    // Mic access is needed if the tenant enables the voice button in embeds.
    iframe.setAttribute("allow", "microphone");

    var targetSel = script.getAttribute("data-target");
    var target = targetSel ? document.querySelector(targetSel) : null;
    if (target) {
      target.appendChild(iframe);
    } else if (script.parentNode) {
      script.parentNode.insertBefore(iframe, script.nextSibling);
    }
  }

  function init() {
    var scripts = document.querySelectorAll("script[data-site][src*='embed.js']");
    Array.prototype.forEach.call(scripts, mount);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
