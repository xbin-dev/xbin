// chat-view.js — the chat as the web draws it: the Session (model/session.js:
// the selected run, its views, the model calls in flight, all kept current by
// one live stream) plus template(), the lit rendering of what it shows
// (chat-cards.js). agent.js owns the page and calls template() to draw the chat
// — a window of its blocks (chat-window.js), or all of them without one.
import { Session as Model } from './model/session.js';
import { sessionTpl } from './chat-cards.js';

export class Session extends Model {
  template(win, s = this.shown()) { return sessionTpl(s, this.ui, win); }
}
