import { expect, test } from '@playwright/test';

test('real Mermaid renders ordinary and mathematical flowcharts', async ({ page }) => {
  await page.goto('/tests/fixtures/dependency-consumers.html');
  const svg = await page.evaluate(async () => {
    const modulePath = '/src/lib/mermaid.ts';
    const { renderMermaid } = await import(modulePath) as {
      renderMermaid: (id: string, text: string) => Promise<string>;
    };
    const plain = await renderMermaid('plain-fixture', 'flowchart LR\n A[Deployment] --> B[Pod]');
    const math = await renderMermaid('math-fixture', 'flowchart LR\n A["$$\\frac{1}{2}$$"] --> B[Pod]');
    return { plain, math };
  });
  expect(svg.plain).toContain('<svg');
  expect(svg.plain).toContain('Deployment');
  expect(svg.plain).toContain('Pod');
  expect(svg.math).toContain('katex');
  expect(svg.math).not.toContain('<script');
});

test('KaTeX does not inherit a trusted-rendering setting from Object.prototype', async ({ page }) => {
  await page.goto('/tests/fixtures/dependency-consumers.html');
  const markup = await page.evaluate(async () => {
    const modulePath = '/node_modules/katex/dist/katex.mjs';
    const { default: katex } = await import(modulePath) as {
      default: { renderToString: (text: string, options: { throwOnError: boolean }) => string };
    };
    const previous = Object.getOwnPropertyDescriptor(Object.prototype, 'trust');
    Object.defineProperty(Object.prototype, 'trust', { value: true, configurable: true, writable: true });
    try {
      return katex.renderToString('\\href{https://example.invalid/fixture}{fixture}', { throwOnError: false });
    } finally {
      if (previous) Object.defineProperty(Object.prototype, 'trust', previous);
      else Reflect.deleteProperty(Object.prototype, 'trust');
    }
  });
  expect(markup).not.toContain('href="https://example.invalid');
  expect(markup).toContain('katex');
});
