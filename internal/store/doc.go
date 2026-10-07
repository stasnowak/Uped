// Package store owns everything on disk under the data directory: reserving
// and appending chunked uploads, finishing them into the shared tree,
// listing, deleting, zipping, the free-space guard and the expiry sweeper.
//
// Phase 0 scaffold: implemented in Phase 1 of plans/uped-home-file-drop.md.
package store
