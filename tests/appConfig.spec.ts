import { test, expect } from './fixtures';
import { testIds } from '../src/components/testIds';

const E2E_KEY = '000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f';

test('should save app configuration', async ({ appConfigPage, page }) => {
  // The config form renders.
  await expect(page.getByTestId(testIds.appConfig.container)).toBeVisible();

  // The API URL is always editable; set it and save.
  const apiUrl = page.getByTestId(testIds.appConfig.apiUrl);
  await apiUrl.clear();
  await apiUrl.fill('https://api.pushward.app');

  // Capture the settings POST before the page reloads on success.
  const saveResponse = appConfigPage.waitForSettingsResponse();
  await page.getByTestId(testIds.appConfig.submit).click();
  await expect(saveResponse).toBeOK();
});

test('saves acknowledge settings and sends only the secret typed', async ({ appConfigPage }) => {
  const page = appConfigPage.ctx.page;
  // Answer the save here so no key is left behind in the test Grafana.
  const saved = new Promise<{ jsonData: Record<string, unknown>; secureJsonData?: Record<string, string> }>(
    (resolve) => {
      page.route('**/api/plugins/*/settings', async (route) => {
        if (route.request().method() !== 'POST') {
          return route.continue();
        }
        resolve(route.request().postDataJSON());
        await route.fulfill({ status: 200, json: { message: 'Plugin settings updated' } });
      });
    }
  );

  await page.getByTestId(testIds.appConfig.alsoNotify).check({ force: true });
  await page.getByTestId(testIds.appConfig.ackEnabled).check({ force: true });
  await page.getByTestId(testIds.appConfig.ackRepeat).fill('120');
  await page.getByTestId(testIds.appConfig.e2eKey).fill(E2E_KEY);
  await page.getByTestId(testIds.appConfig.submit).click();

  const body = await saved;
  expect(body.jsonData).toMatchObject({
    alsoNotify: true,
    ackEnabled: true,
    ackRepeatSeconds: 120,
    ackExpireSeconds: 3600,
  });
  // The API key was not typed, so Grafana keeps the stored one untouched.
  expect(body.secureJsonData).toEqual({ e2eKey: E2E_KEY });
});

test('refuses an integration key in the encryption key field', async ({ appConfigPage }) => {
  const page = appConfigPage.ctx.page;
  let posted = false;
  await page.route('**/api/plugins/*/settings', async (route) => {
    if (route.request().method() !== 'POST') {
      return route.continue();
    }
    posted = true;
    await route.fulfill({ status: 200, json: {} });
  });

  await page.getByTestId(testIds.appConfig.e2eKey).fill('hlk_0123456789abcdef');
  await page.getByTestId(testIds.appConfig.submit).click();

  await expect(page.getByText(/that is an integration key/i)).toBeVisible();
  expect(posted).toBe(false);
});
