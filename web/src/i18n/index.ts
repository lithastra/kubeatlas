import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';

import appEn from './en/app.json';
import glossaryEn from './en/glossary.json';
import translationEn from './en/translation.json';

// Three-namespace organisation, mirroring Headlamp:
//
//   translation - UI strings (buttons, table columns, empty states)
//   glossary    - Kubernetes vocabulary (kind names, edge type labels)
//   app         - project-specific strings (product name, URLs)
//
// English is the only locale shipped in v0.1.0; future locales drop
// into src/i18n/<locale>/ and resources['<locale>'] here. Components
// pick a namespace via useTranslation('translation'), not the
// 'translation:foo' prefix style.
export const defaultNS = 'translation';

void i18n.use(initReactI18next).init({
  resources: {
    en: {
      translation: translationEn,
      glossary: glossaryEn,
      app: appEn,
    },
  },
  lng: 'en',
  fallbackLng: 'en',
  ns: ['translation', 'glossary', 'app'],
  defaultNS,
  interpolation: {
    // React already escapes; double-escaping garbles output.
    escapeValue: false,
  },
});

export default i18n;
