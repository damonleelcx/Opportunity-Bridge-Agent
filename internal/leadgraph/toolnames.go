package leadgraph

// ToolImportSummary names the tool that describes a staged import.
//
// It is the one tool name also written OUTSIDE the tool table: the upload route
// keeps the plan it staged in the conversation under this name, and the
// interface redraws a kept card by it (cardFor in web/static/app.js). A second
// spelling would let a rename here leave every reopened upload drawing nothing.
// See docs/bugfix/2026-09-11-upload-only-conversation-was-hidden.md
const ToolImportSummary = "import_summary"
