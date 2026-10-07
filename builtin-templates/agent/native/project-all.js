// native/project-all.js — the native view's feature modules for Projects
// (API.md §Projects in the UI). native.js imports this file once; each
// module registers its hooks on the native seams (native/ext.js) when
// imported, so a part lands as a new file plus one import line here —
// native.js and native/chat.js stay as they are.
//
// One import per line, each in its own slot below (parallel branches merge
// without touching each other's lines).

// U2 the Projects screens: the list menu's item, the list, a project's board, a new task, issues, activity, a new project
import './projects.js';

// U2 a project's settings: status and sign-in, repos, the policy, members, archive and delete
import './project-settings.js';

// U2 a project's conversation: its branch and PRs, the way back, the prep and sign-in cards; Make this a project…
import './project-task.js';

// U2 team projects: the team board, Work on this, the team's changes to review
import './project-team.js';
