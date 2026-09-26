// ime-field — a Japanese delivery-address form, typed with an input method.
// The app reports a field's text only once a composition commits (never the
// reading while its kanji are being chosen), so the tile may rewrite what it
// receives: the postal code arrives in full-width digits (１５０－０００１)
// and comes back normalized (150-0001) — a set the app applies at once, or
// at the end of a composition still in progress — and a complete code fills
// the prefecture, city and town from a lookup. Name, address and the delivery
// note keep the text as typed.
import { html, render, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

const form = { name: '', postal: '', address: '', note: '' };
let slot = null;
let looked = '';

// full-width digits, hyphens and spaces → ASCII; 7 digits → NNN-NNNN
function postal(v) {
  const ascii = v.replace(/[０-９]/g, (d) => String.fromCharCode(d.charCodeAt(0) - 0xFEE0)).replace(/[－ー―‐−]/g, '-').replace(/\s/g, '');
  const digits = ascii.replace(/\D/g, '');
  return digits.length === 7 ? `${digits.slice(0, 3)}-${digits.slice(3)}` : ascii;
}

async function lookup(code) {
  if (looked === code) return;
  looked = code;
  const a = await selfApi(`/postal/${code.replace('-', '')}`);
  if (!form.address) form.address = `${a.prefecture}${a.city}${a.town}`;
  paint();
}

async function load() { slot = await selfApi('/delivery/slot'); paint(); }
const set = (k) => (e) => { form[k] = e.value; paint(); };

const paint = () => render(!slot ? nothing : html`
  <screen title="お届け先" style="form">
    <section title="宛先">
      <field label="氏名" placeholder="山田 太郎" value=${form.name} @input=${set('name')}/>
      <field label="郵便番号" placeholder="123-4567" value=${form.postal} hint="全角で入力しても半角に直ります"
             @input=${(e) => { form.postal = postal(e.value); if (/^\d{3}-\d{4}$/.test(form.postal)) lookup(form.postal); paint(); }}/>
      <field label="住所" kind="multiline" placeholder="都道府県から番地・建物名まで" value=${form.address} @input=${set('address')}/>
    </section>
    <section title="配達メモ" footer="置き配を選ぶと、受け取りのサインは不要です。">
      <field kind="multiline" placeholder="置き配の場所など" value=${form.note} @input=${set('note')}/>
    </section>
    <section>
      <row title="お届け予定" detail=${slot.label} icon="calendar"/>
      <button role="primary" ?disabled=${!form.name || !/^\d{3}-\d{4}$/.test(form.postal) || !form.address} @tap=${() => {}}>この住所に届ける</button>
    </section>
  </screen>`);

load();
