import { test, expect } from './fixtures';
import { testIds } from '../src/components/testIds';

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
