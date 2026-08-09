(function () {
  "use strict"
  var defaults = {
    "vite-ui-theme": "dark",
    "theme_font": "serif",
    "theme_radius": "md",
    "theme_scale": "lg",
    "theme_content_layout": "centered",
    "layout_variant": "floating",
    "dir": "ltr"
  }
  var existing = {}
  document.cookie.split(";").forEach(function (item) {
    var key = item.trim().split("=")[0]
    if (key) existing[key] = true
  })
  Object.keys(defaults).forEach(function (key) {
    if (!existing[key]) {
      document.cookie = key + "=" + defaults[key] + "; Path=/; Max-Age=31536000; SameSite=Lax; Secure"
    }
  })
  if (!existing["vite-ui-theme"]) {
    document.documentElement.classList.remove("light")
    document.documentElement.classList.add("dark")
  }

  var recoveryKey = "madapi_frontend_asset_recovery_at"
  var staleAssetPattern = /ChunkLoadError|Loading chunk .+ failed|Failed to fetch dynamically imported module|Importing a module script failed|Unable to preload CSS/i
  function errorMessage(value) {
    if (!value) return ""
    if (typeof value === "string") return value
    return String(value.name || "") + ": " + String(value.message || "")
  }
  function recover(event, value, force) {
    if (!force && !staleAssetPattern.test(errorMessage(value))) return
    if (event && typeof event.preventDefault === "function") event.preventDefault()
    var now = Date.now()
    try {
      var previous = Number(window.sessionStorage.getItem(recoveryKey) || 0)
      if (now - previous < 60000) return
      window.sessionStorage.setItem(recoveryKey, String(now))
    } catch (_) {
      // A reload remains safer than leaving a stale tab on the generic 500 route.
    }
    window.location.reload()
  }
  window.addEventListener("vite:preloadError", function (event) {
    recover(event, event.payload, true)
  })
  window.addEventListener("unhandledrejection", function (event) {
    recover(event, event.reason, false)
  })
})()
