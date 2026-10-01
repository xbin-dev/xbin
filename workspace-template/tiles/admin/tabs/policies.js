/**
 * <bx-admin-policies> — what was the admin console's workspace → policies tab
 * (PD-55) is the partitioned tiles' section of the settings tab since D180
 * (tabs/settings.js). This element is that section alone, kept for an
 * admin.js from before the settings tab: nothing shipped is removed
 * (docs/compat.md rule 4).
 */
import { BxAdminSettings } from './settings.js';

export class BxAdminPolicies extends BxAdminSettings {
  constructor() { super(); this.only = 'partitions'; }
}
customElements.define('bx-admin-policies', BxAdminPolicies);
