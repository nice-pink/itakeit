// Server entry for build-time prerendering (scripts/prerender.js). Crawlers
// that do not run JavaScript, most AI answer engines among them, read this HTML.
import { render } from 'svelte/server'
import App from './App.svelte'
import { image, repo, site } from './lib/site'

const author = { '@type': 'Organization', '@id': 'https://nice.pink/#org', name: 'nice-pink', url: 'https://nice.pink/' }

const app = {
  '@type': 'SoftwareApplication',
  name: 'itakeit',
  url: `${site}/`,
  image: `${site}/og-image.png`,
  applicationCategory: 'BusinessApplication',
  applicationSubCategory: 'Task tracking',
  operatingSystem: 'Linux (Docker)',
  description: 'A free, self-hosted Slack bot that turns every message in one channel into a task. People claim it with a 🙋 reaction, owners set the status with reactions, and a pinned board lists every open task.',
  downloadUrl: image,
  softwareHelp: repo,
  isAccessibleForFree: true,
  offers: { '@type': 'Offer', price: '0', priceCurrency: 'EUR' },
  author: { '@id': author['@id'] },
  isRelatedTo: { '@type': 'SoftwareApplication', name: 'Betimation', url: 'https://betimation.com/', description: 'Advanced and fun team task tracking.' },
}

const ld = (graph: object[]) => JSON.stringify({ '@context': 'https://schema.org', '@graph': graph }).replace(/</g, '\\u003c')

export const pages = [
  { file: 'index.html', render: () => render(App), jsonLd: ld([author, { '@type': 'WebSite', name: 'itakeit', url: `${site}/`, publisher: { '@id': author['@id'] } }, app]) },
]
