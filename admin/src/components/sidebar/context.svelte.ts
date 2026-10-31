// Adapted from Arcane's SidebarState; preferences are local to CineRoute admin.
import { getContext, setContext } from 'svelte';
export class SidebarState {
	pinned = $state(true);
	hovered = $state(false);
	mobile = $state(false);
	tablet = $state(false);
	drawer = $state(false);
	hoverExpansionEnabled = true;
	private timer: ReturnType<typeof setTimeout> | undefined;
	get state() {
		return this.isExpanded ? 'expanded' : 'collapsed';
	}
	get isHovered() {
		return this.hovered;
	}
	get isExpanded() {
		return this.mobile ? this.drawer : (!this.tablet && this.pinned) || this.hovered;
	}
	get gap() {
		return this.mobile ? '0px' : this.tablet || !this.pinned ? 'calc(3rem + 16px)' : '17rem';
	}
	toggle() {
		if (this.mobile) {
			this.drawer = !this.drawer;
			return;
		}
		if (this.tablet) return;
		this.pinned = !this.pinned;
		this.hovered = false;
		try {
			localStorage.setItem('cineroute-sidebar-pinned', String(this.pinned));
		} catch {}
	}
	setHovered(value: boolean, delay = 0) {
		clearTimeout(this.timer);
		if (delay) this.timer = setTimeout(() => (this.hovered = value), delay);
		else this.hovered = value;
	}
	close() {
		this.drawer = false;
		this.hovered = false;
	}
	destroy() {
		clearTimeout(this.timer);
	}
}
const key = Symbol('sidebar');
export const setSidebar = (sidebar: SidebarState) => setContext(key, sidebar);
export const useSidebar = () => getContext<SidebarState>(key);
