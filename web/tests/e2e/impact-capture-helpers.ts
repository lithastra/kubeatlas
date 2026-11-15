import { readFile } from 'node:fs/promises';
import { expect, type Page } from '@playwright/test';

export async function download(page: Page, format: 'JSON' | 'HTML') {
  const event = page.waitForEvent('download');
  await page.getByRole('button', { name: `Download ${format}`, exact: true }).click();
  const result = await event;
  expect(await result.failure()).toBeNull();
  const path = await result.path();
  expect(path).not.toBeNull();
  return readFile(path!, 'utf8');
}

export async function embeddedJSON(page: Page, html: string) {
  return page.evaluate((source) => {
    const doc = new DOMParser().parseFromString(source, 'text/html');
    if (doc.querySelector('script,iframe,object,embed,link,img,form,[src],[href]')) throw new Error('Active/external report content');
    if (!doc.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute('content')?.includes("default-src 'none'")) throw new Error('Missing report CSP');
    return doc.getElementById('captured-json')?.textContent;
  }, html);
}

export async function observeImpactResponse(page: Page, target: {
  origin: string;
  pathname: string;
  relation: string;
  depth: number;
  cluster: string;
}) {
  let captured: { status: number; raw: string; url: string } | undefined;
  await page.exposeFunction('captureImpactResponse', (status: number, raw: string, url: string) => {
    captured = { status, raw, url };
  });
  await page.evaluate((target) => {
    const nativeFetch = window.fetch.bind(window);
    const capture = (window as typeof window & {
      captureImpactResponse: (status: number, raw: string, url: string) => Promise<void>;
    }).captureImpactResponse;
    window.fetch = async (...args: Parameters<typeof fetch>) => {
      const response = await nativeFetch(...args);
      const url = new URL(response.url);
      if (url.origin === target.origin && url.pathname === target.pathname &&
          url.searchParams.get('relation') === target.relation &&
          url.searchParams.get('max_depth') === String(target.depth) &&
          (url.searchParams.get('cluster') || '') === target.cluster) {
        // Drain a native clone before the application's cleanup aborts the fetch.
        // Return the original response with its body, headers and status intact.
        await capture(response.status, await response.clone().text(), url.href);
      }
      return response;
    };
  }, target);
  return () => captured;
}
