import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

const config: Config = {
  title: 'KubeAtlas',
  tagline: 'Kubernetes resource dependency graph tool',
  favicon: 'img/favicon.ico',

  future: {
    v4: true,
  },

  url: 'https://docs.kubeatlas.lithastra.com',
  baseUrl: '/',

  organizationName: 'lithastra',
  projectName: 'kubeatlas',

  onBrokenLinks: 'throw',

  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl: 'https://github.com/lithastra/kubeatlas/tree/main/docs/',
          routeBasePath: '/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'KubeAtlas',
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Docs',
        },
        {
          href: 'https://github.com/lithastra/kubeatlas',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {label: 'What is KubeAtlas', to: '/'},
            {label: 'Quick Start', to: '/quick-start'},
            {label: 'Helm install options', to: '/installation/helm'},
            {label: 'Architecture', to: '/architecture'},
            {label: 'API reference', to: '/api-reference'},
            {label: 'Developer Guide', to: '/developer-guide'},
            {label: 'FAQ', to: '/faq'},
            {label: 'Roadmap', to: '/roadmap'},
          ],
        },
        {
          title: 'Project',
          items: [
            {
              label: 'GitHub',
              href: 'https://github.com/lithastra/kubeatlas',
            },
            {
              label: 'Issues',
              href: 'https://github.com/lithastra/kubeatlas/issues',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Lithastra. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'yaml', 'go', 'json'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
