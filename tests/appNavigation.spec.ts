import { test, expect } from './fixtures';
import { ROUTES } from '../src/constants';
import { testIds } from '../src/components/testIds';

test.describe('navigating the PushWard app', () => {
  test('overview page renders', async ({ gotoPage, page }) => {
    await gotoPage(`/${ROUTES.Overview}`);
    await expect(page.getByTestId(testIds.overview.container)).toBeVisible();
  });

  test('connect page renders', async ({ gotoPage, page }) => {
    await gotoPage(`/${ROUTES.Connect}`);
    await expect(page.getByTestId(testIds.connect.container)).toBeVisible();
  });

  test('activities page renders', async ({ gotoPage, page }) => {
    await gotoPage(`/${ROUTES.Activities}`);
    await expect(page.getByTestId(testIds.activities.container)).toBeVisible();
  });
});
