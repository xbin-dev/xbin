# apps/calendar API

Calendar events for the workspace. The reference example of a scope exposing
a service API + a bus other apps can tap into (the "email reads calendar"
story from the xbin docs).

## Roles

| Role   | Grants |
|--------|--------|
| reader | Read events and calendars |
| writer | Create and modify events |
| admin  | Manage calendar settings |

## Endpoints

### GET /events?day=YYYY-MM-DD — role: reader

Day defaults to today.

```json
{"day":"2026-07-02","events":[{"id":"2026-07-02/17513…","day":"2026-07-02","time":"09:30","title":"standup"}]}
```

### POST /events — role: writer

Body: `{"day":"YYYY-MM-DD","time":"HH:MM","title":"…"}`. Returns the created
event. Publishes `events/created` on `res:apps/calendar/bus`.

## Bus topics — resource `res:apps/calendar/bus` (role: reader to subscribe)

| Topic | Payload |
|-------|---------|
| `events/created` | the created event object |

## Use it

```jsonc
// caller's xbin.json
{
  "uses": [
    { "target": "apps/calendar",         "role": "reader" },
    { "target": "res:apps/calendar/bus", "role": "reader" }
  ]
}
```

```go
resp, _ := xbin.Client().Get("http://xbin/api/apps/calendar/events?day=2026-07-02")
```

```js
// frontend
xbin.bus.on('res:apps/calendar/bus/events/', (topic, ev) => refresh());
```
