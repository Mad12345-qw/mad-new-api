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

  var style = document.createElement("style")
  style.id = "mad-solid-public-header"
  style.textContent = 'header[class*="fixed"] nav { background-color: #1d1d1d !important; }'
  document.head.appendChild(style)
})()
