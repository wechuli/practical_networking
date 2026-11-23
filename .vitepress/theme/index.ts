import DefaultTheme from 'vitepress/theme'
import { inBrowser } from 'vitepress'
import type { Theme } from 'vitepress'
import './custom.css'

export default {
  extends: DefaultTheme,
  enhanceApp() {
    if (!inBrowser) return
    // Diagrams open full size in a new tab when clicked.
    document.addEventListener('click', (event) => {
      const target = event.target
      if (target instanceof HTMLImageElement && target.closest('.vp-doc') && target.src.endsWith('.svg')) {
        window.open(target.src, '_blank', 'noopener')
      }
    })
  },
} satisfies Theme
