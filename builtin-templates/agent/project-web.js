// project-web.js — the web's feature modules for Projects (API.md §Projects
// in the UI). agent.js imports this file once; each module registers its
// hooks on the web's seams (web-ext.js) when imported, so a part lands as a
// new file plus one import line here — agent.js and the other hot files stay
// as they are.
//
// One import per line, each in its own slot below (parallel branches merge
// without touching each other's lines).

// U1 the Projects page: the list, a project's board, new tasks and tasks from issues (projects.js
// draws the new-project form, project-new.js, and the Settings tab, project-settings.js)
import './projects.js';

// U1 a project's conversation: its chips, crumb, prep and sign-in cards, the pinned task's project
import './project-chips.js';

// U2 the coordinator, the event feed, upgrades and team projects
