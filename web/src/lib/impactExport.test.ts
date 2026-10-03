import { impactFixture } from '../test/impactFixture';
import { impactCaptureJSON, parseImpactCapture } from '../api/impactCapture';
import { downloadImpactCapture, renderImpactExport } from './impactExport';

test('JSON is the original capture; HTML contains the identical capture and escapes malicious text', () => {
  const fixture = impactFixture();
  fixture.kubeatlasVersion = '</pre><script>alert(1)</script><img src="https://invalid.test/private">';
  const raw = JSON.stringify(fixture, null, 2).replace('604800000000000', '9223372036854775807');
  const response = parseImpactCapture(raw);
  expect(renderImpactExport(response, 'json').content).toBe(raw);
  const report = renderImpactExport(response, 'html');
  const doc = new DOMParser().parseFromString(report.content, 'text/html');
  expect(doc.querySelector('#captured-json')?.textContent).toBe(raw);
  expect(doc.querySelectorAll('script,img,link,iframe,object,audio,video,form')).toHaveLength(0);
  expect(doc.querySelector('meta[http-equiv="Content-Security-Policy"]')?.getAttribute('content')).toContain("default-src 'none'");
  expect(doc.body.textContent).toContain(fixture.kubeatlasVersion);
  expect(doc.body.textContent).toContain('2 observed · 1 direct · 1 indirect');
  expect(doc.body.textContent).toContain('not effective permissions');
  expect(doc.body.textContent).toContain('not backups or verified recovery points');
  expect(report.filename).toMatch(/^kubeatlas-impact-[a-zA-Z0-9]+\.html$/);
});

test('unverified or copied response objects cannot be exported', () => {
  const fixture = impactFixture();
  expect(() => renderImpactExport(fixture, 'json')).toThrow('No verified');
  const capture = parseImpactCapture(JSON.stringify(fixture));
  expect(() => renderImpactExport({ ...capture }, 'html')).toThrow('No verified');
});

test('empty and truncated captures retain their limitations', () => {
  const fixture = impactFixture();
  fixture.analysis.resources = [];
  fixture.analysis.referenceEvidence = [];
  Object.assign(fixture.analysis.counts, { total: 0, direct: 0, indirect: 0, pods: 0, other: 0 });
  fixture.analysis.truncated = true;
  fixture.analysis.truncationReasons = ['max_depth'];
  const page = renderImpactExport(parseImpactCapture(JSON.stringify(fixture)), 'html').content;
  expect(page).toContain('No dependents observed; analysis incomplete.');
  expect(page).toContain('Truncated: max_depth');
  expect(page).not.toContain('No dependents found');
});

test('download triggers no fetch and releases its temporary anchor and object URL', () => {
  jest.useFakeTimers();
  const create = jest.fn().mockReturnValue('blob:synthetic');
  const revoke = jest.fn();
  const originalCreate = URL.createObjectURL;
  const originalRevoke = URL.revokeObjectURL;
  URL.createObjectURL = create;
  URL.revokeObjectURL = revoke;
  const click = jest.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
  const response = parseImpactCapture(JSON.stringify(impactFixture()));
  try {
    downloadImpactCapture(response, 'json');
    expect(click).toHaveBeenCalledTimes(1);
    expect(create).toHaveBeenCalledWith(expect.any(Blob));
    expect(document.querySelector('a[download]')).toBeNull();
    expect(impactCaptureJSON(response)).toBe(JSON.stringify(impactFixture()));
    jest.runAllTimers();
    expect(revoke).toHaveBeenCalledWith('blob:synthetic');
    click.mockImplementation(() => { throw new Error('blocked'); });
    expect(() => downloadImpactCapture(response, 'html')).toThrow('blocked');
    jest.runAllTimers();
    expect(revoke).toHaveBeenCalledTimes(2);
    expect(document.querySelector('a[download]')).toBeNull();
  } finally {
    URL.createObjectURL = originalCreate;
    URL.revokeObjectURL = originalRevoke;
    click.mockRestore();
    jest.useRealTimers();
  }
});
