// Package inbox gathers what needs someone's attention, for the attention
// inbox on the panel's home screen: work that failed, resources that aren't
// working, alerts, updates waiting to be installed, certificates about to
// expire or that couldn't be renewed, and modules that would help.
//
// Sources find the items. Some depend on who asks and are read for every
// request, such as failed jobs, which the job queue answers quickly. Others
// find the same for everyone and take a while, such as the packages apt
// would upgrade; the service keeps what they found and checks again in the
// background once that is old. Modules add sources of their own through
// modules.InboxProvider. The API decides who may see each item from its
// Access before it shows any.
//
// People dismiss items they have dealt with, or snooze them for a while.
// A dismissal holds until the item changes: an item's Version is different
// once there is something new to see, such as another failure, and the
// item comes back. Dismissals change what one person sees and nothing
// Racksail manages, so they don't go through the change pipeline.
package inbox
