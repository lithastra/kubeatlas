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
        'installation/persistence',
        'installation/openshift',
        'installation/cert-manager',
        'installation/eks',
      ],
    },
    {
      type: 'category',
      label: 'Concepts',
      collapsed: false,
      items: [
        'concepts/blast-radius',
        'concepts/orphan-cycle',
        'concepts/rego-rules',
        {
          type: 'category',
          label: 'Rule packs',
          items: [
            'concepts/rule-packs/overview',
            'concepts/rule-packs/istio',
            'concepts/rule-packs/argocd',
            'concepts/rule-packs/knative',
            'concepts/rule-packs/strimzi',
            'concepts/rule-packs/velero',
            'concepts/rule-packs/tekton',
          ],
        },
        'concepts/api-versioning',
        'concepts/snapshots',
        'concepts/rule-pack-security',
      ],
    },
    'architecture',
    'api-reference',
    'cli-reference',
    'developer-guide',
    'faq',
    'roadmap',
  ],
};

export default sidebars;
