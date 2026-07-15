(function () {
  'use strict'

  var canvas = document.getElementById('networkCanvas')
  var context = canvas.getContext('2d', { alpha: true })
  var reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  var points = []
  var frameId = 0
  var width = 0
  var height = 0
  var pixelRatio = Math.min(window.devicePixelRatio || 1, 2)

  function createPoints() {
    var count = Math.max(24, Math.min(54, Math.floor(width / 30)))
    points = Array.from({ length: count }, function (_, index) {
      var lane = index % 3
      return {
        x: Math.random() * width,
        y: Math.random() * height,
        vx: (Math.random() - 0.5) * (lane === 0 ? 0.18 : 0.1),
        vy: (Math.random() - 0.5) * 0.12,
        radius: lane === 0 ? 1.8 : 1.1,
        color: lane === 0 ? '#70e8d2' : lane === 1 ? '#c7f36b' : '#ff806e',
      }
    })
  }

  function resizeCanvas() {
    var rect = canvas.getBoundingClientRect()
    width = Math.max(1, Math.floor(rect.width))
    height = Math.max(1, Math.floor(rect.height))
    canvas.width = Math.floor(width * pixelRatio)
    canvas.height = Math.floor(height * pixelRatio)
    context.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0)
    createPoints()
  }

  function draw() {
    context.clearRect(0, 0, width, height)

    for (var i = 0; i < points.length; i += 1) {
      var point = points[i]
      if (!reducedMotion) {
        point.x += point.vx
        point.y += point.vy
        if (point.x < -20) point.x = width + 20
        if (point.x > width + 20) point.x = -20
        if (point.y < -20) point.y = height + 20
        if (point.y > height + 20) point.y = -20
      }

      for (var j = i + 1; j < points.length; j += 1) {
        var target = points[j]
        var dx = point.x - target.x
        var dy = point.y - target.y
        var distance = Math.sqrt(dx * dx + dy * dy)
        if (distance < 150) {
          context.beginPath()
          context.moveTo(point.x, point.y)
          context.lineTo(target.x, target.y)
          context.strokeStyle = 'rgba(157, 190, 181, ' + (0.12 * (1 - distance / 150)) + ')'
          context.lineWidth = 1
          context.stroke()
        }
      }

      context.beginPath()
      context.arc(point.x, point.y, point.radius, 0, Math.PI * 2)
      context.fillStyle = point.color
      context.globalAlpha = 0.72
      context.fill()
      context.globalAlpha = 1
    }

    if (!reducedMotion) frameId = window.requestAnimationFrame(draw)
  }

  function showToast(message) {
    var toast = document.getElementById('toast')
    toast.textContent = message
    toast.classList.add('visible')
    window.setTimeout(function () {
      toast.classList.remove('visible')
    }, 1800)
  }

  document.getElementById('copyApiUrl').addEventListener('click', function () {
    var value = document.getElementById('apiBaseUrl').textContent
    navigator.clipboard.writeText(value).then(
      function () {
        showToast('API 地址已复制')
      },
      function () {
        showToast('复制失败，请手动选择地址')
      }
    )
  })

  window.addEventListener('resize', function () {
    window.cancelAnimationFrame(frameId)
    resizeCanvas()
    draw()
  })

  resizeCanvas()
  draw()
})()
