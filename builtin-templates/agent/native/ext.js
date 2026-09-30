// native/ext.js — the native view's seams (model/ext.js): where a feature
// module draws into the conversation, its toolbar, menu and composer, the
// new-chat sheet and pushed screens without editing native.js or
// native/chat.js. Register from a module native/harness-all.js imports:
//
//   import { ext } from './ext.js';
//   ext.register({ block: (b, depth) => …, screen: (s) => … });
//
// Each hook answers an xb-native template (or null: not mine).
//   block(b, depth)  a transcript block (model/fold.js) drawn your way; the
//                    first answer replaces the built-in one (native/chat.js)
//   end(v, s)        after the transcript's rows (s = Session.shown()), before
//                    the built-in approval, question and activity line. A
//                    harness park (run.pendingState.harness) is left to these
//                    hooks: while any answers, the built-in approval and
//                    question are not drawn for it
//   toolbar(v)       items in the toolbar (v the open view; null at home)
//   subtitle(v)      words for the conversation's subtitle (a string; after its
//                    chain, before its status) — a phone's bar holds few items
//   menu(v, t)       items in the conversation's ⋯ menu (t = rules.topBar(v))
//   main(before)     items in the main ⋯ menu (home's toolbar, the drawer's);
//                    before() runs first when one is tapped (the drawer closes)
//   composer(v, t)   {placeholder?, slash?: [{name, hint, description}],
//                    tpl?()} — the last placeholder given wins, the slash
//                    commands add up, tpl() draws buttons into the composer
//                    (v null at home)
//   newChat(f)       {tpl(), body()} — tpl() draws a section of the new-chat
//                    sheet (f its form: keep your field's value in it),
//                    body() is merged into the POST /ask it sends
//   screen(s)        a pushed screen (ui.stack entry s = {kind, …}) of a kind
//                    native/tools.js doesn't know — push({kind: 'mine', …})
import { makeExt } from '../model/ext.js';

export const ext = makeExt({ block: 'first', end: 'all', toolbar: 'all', subtitle: 'all', menu: 'all', main: 'all', composer: 'all', newChat: 'all', screen: 'first' });
