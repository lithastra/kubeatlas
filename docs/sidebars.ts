import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docsSidebar: [
    'intro',
    'quick-start',
    {
      type: 'category',
      label: 'Installation',
      collapsed: false,
      items: [
        'installation/helm',
        'installation/security-warning',
        'installation/ingress-nginx-f5',
        'installation/ingress-traefik',
        'installation/ingress-alb',
        'installation/ingress-nginx-eol-notice',
      ],
    },
    'architecture',
    'api-reference',
    'developer-guide',
    'faq',
    'roadmap',
  ],
};

export default sidebars;
