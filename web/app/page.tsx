import SearchView from './search-view';
import StatusBarContainer from '@/components/status-bar/container';
import { StatusBarProvider } from '@/lib/status-bar/context';
import { Toaster } from '@/components/ui/toaster';
import UserMenu from '@/components/user-menu';

export default function Home() {
    return (
        <StatusBarProvider>
            <div className='flex flex-col min-h-screen'>
                <UserMenu />
                <main className='cineroute-main px-6 flex-1 flex flex-col items-center gap-2 z-[2] overflow-x-hidden max-w-screen overflow-y-hidden'>
                    <SearchView />
                </main>
                <Toaster />
                <StatusBarContainer />
            </div>
        </StatusBarProvider>
    );
}
