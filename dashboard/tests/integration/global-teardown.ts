import { stopHarness } from './_harness';

export default async function globalTeardown() {
  await stopHarness();
}
