// Keep touch navigation at the page's normal scale, including iOS Safari,
// which can ignore the viewport's user-scalable setting for pinch gestures.
(() => {
  const preventZoom = event => event.preventDefault();
  for (const name of ['gesturestart', 'gesturechange']) {
    document.addEventListener(name, preventZoom, { passive: false });
  }
  const preventPinch = event => {
    if (event.touches.length > 1) event.preventDefault();
  };
  document.addEventListener('touchstart', preventPinch, { passive: false });
  document.addEventListener('touchmove', preventPinch, { passive: false });
})();
