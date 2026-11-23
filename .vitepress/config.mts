import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vitepress'

const repo = 'https://github.com/wechuli/practical_networking'
// GitHub Pages tells the workflow which base path the site lives under.
const base = process.env.SITE_BASE ?? '/practical_networking/'
// Links to repository files point at the commit being built, or main locally.
const ref = process.env.GITHUB_SHA ?? 'main'
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

const modules: Array<[number, string, string | null]> = [
  [0, 'Before we begin', 'module-00-before-we-begin'],
  [1, 'The history of networking', 'module-01-history-of-networking'],
  [2, 'Quickstart: a webpage end to end', 'module-02-quickstart-a-webpage-end-to-end'],
  [3, 'Networking fundamentals', 'module-03-networking-fundamentals'],
  [4, 'The OSI model', 'module-04-the-osi-model'],
  [5, 'IP: the Internet Protocol', 'module-05-ip-the-internet-protocol'],
  [6, 'ARP: Address Resolution Protocol', 'module-06-arp-address-resolution-protocol'],
  [7, 'ICMP, ping and traceroute', 'module-07-icmp-ping-and-traceroute'],
  [8, 'Routing', 'module-08-routing'],
  [9, 'BGP, anycast and internet routing', 'module-09-bgp-anycast-and-internet-routing'],
  [10, 'UDP: User Datagram Protocol', 'module-10-udp-user-datagram-protocol'],
  [11, 'TCP: Transmission Control Protocol', 'module-11-tcp-transmission-control-protocol'],
  [12, 'DNS: Domain Name System', 'module-12-dns-domain-name-system'],
  [13, 'TLS: Transport Layer Security', 'module-13-tls-transport-layer-security'],
  [14, 'Network performance', 'module-14-network-performance'],
  [15, 'HTTP/2, HTTP/3, gRPC and WebSockets', 'module-15-http2-http3-grpc-websockets'],
  [16, 'Networking in Docker and containers', 'module-16-networking-in-docker-and-containers'],
  [17, 'Kubernetes networking', 'module-17-kubernetes-networking'],
  [18, 'Firewalls, security and zero trust', 'module-18-firewalls-security-and-zero-trust'],
  [19, 'VPNs, tunnels and overlay networks', 'module-19-vpns-tunnels-and-overlay-networks'],
  [20, 'Sockets and application-level networking', 'module-20-sockets-and-application-level-networking'],
  [21, 'Packet capture and analysis', 'module-21-packet-capture-and-analysis'],
  [22, 'Observability and troubleshooting', 'module-22-observability-and-troubleshooting'],
  [23, 'Additional critical topics', 'module-23-additional-critical-topics'],
  [24, 'Linux kernel networking internals (planned)', null],
  [25, 'Cloud and CI/CD path troubleshooting (planned)', null],
  [26, 'Capstone labs', 'module-26-capstone-labs'],
]

function moduleItems(from: number, to: number) {
  return modules
    .filter(([n]) => n >= from && n <= to)
    .map(([n, title, slug]) => {
      const text = `${String(n).padStart(2, '0')} · ${title}`
      return slug ? { text, link: `/modules/${slug}` } : { text }
    })
}

// Relative links to non-Markdown files (Go sources, lab JSON, scripts, folders)
// are not pages on the site, so they point at the file on GitHub instead.
const notRelative = /^([a-zA-Z][a-zA-Z0-9+.-]*:|[#/])/

function repoFileLink(href: string, page: string): string | null {
  if (notRelative.test(href)) return null
  const [target, ...hash] = href.split('#')
  if (!target || /\.(md|html)$/i.test(target)) return null
  const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(page), decodeURI(target)))
  if (resolved === '..' || resolved.startsWith('../')) return null
  const full = path.join(root, resolved)
  if (!fs.existsSync(full)) throw new Error(`${page}: link to missing file ${href}`)
  const kind = fs.statSync(full).isDirectory() ? 'tree' : 'blob'
  const suffix = resolved === '.' ? '' : `/${encodeURI(resolved)}`
  return `${repo}/${kind}/${ref}${suffix}${hash.length ? `#${hash.join('#')}` : ''}`
}

export default defineConfig({
  title: 'Practical Networking',
  description:
    'A hands-on networking course: from Ethernet and IP to TCP, DNS, TLS, HTTP/2–3, containers, Kubernetes, firewalls, VPNs, packet capture and incident diagnosis.',
  base,
  lang: 'en',
  cleanUrls: true,
  lastUpdated: true,
  srcExclude: ['SUMMARY.md', 'node_modules/**', 'tools/**', '.github/**'],
  ignoreDeadLinks: ['localhostLinks'],
  head: [['link', { rel: 'icon', href: `${base}favicon.svg`, type: 'image/svg+xml' }]],
  markdown: {
    config(md) {
      // Inline code like `docker inspect -f '{{.State.Pid}}'` must not be read as Vue syntax.
      const codeInline = md.renderer.rules.code_inline!
      md.renderer.rules.code_inline = (tokens, idx, options, env, self) => {
        tokens[idx].attrSet('v-pre', '')
        return codeInline(tokens, idx, options, env, self)
      }
      md.core.ruler.after('inline', 'repo-file-links', (state) => {
        const page: string = state.env.relativePath
        for (const block of state.tokens) {
          for (const token of block.children ?? []) {
            if (token.type !== 'link_open') continue
            const url = repoFileLink(token.attrGet('href') ?? '', page)
            if (url) token.attrSet('href', url)
          }
        }
      })
    },
  },
  vite: {
    // Keep diagrams as files: never inline them as data URIs.
    build: { assetsInlineLimit: 0 },
  },
  themeConfig: {
    logo: '/favicon.svg',
    nav: [
      { text: 'Start here', link: '/modules/module-00-before-we-begin' },
      { text: 'Labs', link: '/labs/' },
      { text: 'Reference', link: '/appendices' },
    ],
    sidebar: [
      {
        text: 'Start here',
        items: [
          { text: 'Welcome', link: '/' },
          { text: 'Introduction', link: '/README' },
        ],
      },
      { text: 'Foundations', collapsed: false, items: moduleItems(0, 4) },
      { text: 'Addressing and paths', collapsed: false, items: moduleItems(5, 9) },
      { text: 'Transport and applications', collapsed: false, items: moduleItems(10, 15) },
      { text: 'Production', collapsed: false, items: moduleItems(16, 23) },
      { text: 'Going further', collapsed: false, items: moduleItems(24, 26) },
      {
        text: 'Labs',
        collapsed: true,
        items: [
          { text: 'Lab contract', link: '/labs/README' },
          { text: 'Objective-to-exercise index', link: '/labs/' },
          { text: 'Go lab toolbox', link: '/labs/go/README' },
          { text: 'Routing evidence exercise', link: '/labs/routing' },
          { text: 'Switching exercises', link: '/labs/overlays' },
          { text: 'Tailscale minicourse', link: '/labs/tailscale' },
          { text: 'Private service access walkthrough', link: '/labs/cloud-private-access' },
          { text: 'Kubernetes lab profile', link: '/labs/kubernetes/README' },
          { text: 'Instructor setup and solutions', link: '/labs/capstones/instructor' },
          { text: 'Incident submission', link: '/labs/capstones/submission' },
        ],
      },
      {
        text: 'Reference',
        collapsed: true,
        items: [
          { text: 'Appendices', link: '/appendices' },
          { text: 'Course outline', link: '/networking_course_outline' },
        ],
      },
      {
        text: 'About this course',
        collapsed: true,
        items: [
          { text: 'Course improvement implementation', link: '/implementation' },
          { text: 'Course review and recommendations', link: '/improvements' },
        ],
      },
    ],
    outline: { level: [2, 3] },
    search: { provider: 'local' },
    socialLinks: [{ icon: 'github', link: repo }],
    editLink: {
      pattern: `${repo}/edit/main/:path`,
      text: 'Suggest a change to this page',
    },
    footer: {
      message: 'Course content is MIT-licensed.',
      copyright: '© 2026 Paul Wechuli',
    },
  },
})
