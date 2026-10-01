/**
 * <bx-admin-terminals> — what was the admin console's workspace → terminals
 * tab (base auto-update, D175) is the terminals section of the settings tab
 * since D180 (tabs/settings.js). This element is that section alone, kept
 * for an admin.js from before the settings tab: nothing shipped is removed
 * (docs/compat.md rule 4).
 */
import { BxAdminSettings } from './settings.js';

export class BxAdminTerminals extends BxAdminSettings {
  constructor() { super(); this.only = 'terminals'; }
}
customElements.define('bx-admin-terminals', BxAdminTerminals);
