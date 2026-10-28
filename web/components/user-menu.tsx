'use client';

import { useT } from '@/lib/i18n';
import { useEffect, useState } from 'react';
import { LogOut, UserRound } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuLabel,
    DropdownMenuSeparator,
    DropdownMenuTrigger
} from '@/components/ui/dropdown-menu';
import { getMe, isUnauthorized, logout, formatApiError, type Session } from '@/lib/cineroute-api';
import { toast } from '@/hooks/use-toast';

export default function UserMenu() {
    const t = useT();
    const [session, setSession] = useState<Session | null>(null);
    const [loggingOut, setLoggingOut] = useState(false);

    useEffect(() => {
        let active = true;
        void getMe()
            .then((session) => {
                if (active) setSession(session);
            })
            .catch(() => {});
        return () => {
            active = false;
        };
    }, []);

    const signOut = async () => {
        setLoggingOut(true);
        try {
            await logout();
            window.location.replace('/login');
        } catch (error) {
            if (!isUnauthorized(error))
                toast({ variant: 'destructive', localized: (t) => ({ title: t('logOutFailed'), description: formatApiError(error, t) }) });
            setLoggingOut(false);
        }
    };

    if (!session?.auth) return null;

    return (
        <div className='user-menu absolute top-4 right-4 z-30'>
            <DropdownMenu>
                <DropdownMenuTrigger asChild>
                    <Button variant='ghost' size='icon' aria-label={t('userMenu')}>
                        <UserRound className='size-5' />
                    </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align='end' className='max-w-[calc(100vw-2rem)]'>
                    <DropdownMenuLabel className='break-words'>{t('signedIn', { user: session.user })}</DropdownMenuLabel>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem disabled={loggingOut} onSelect={() => void signOut()}>
                        <LogOut />
                        {loggingOut ? t('loggingOut') : t('logOut')}
                    </DropdownMenuItem>
                </DropdownMenuContent>
            </DropdownMenu>
        </div>
    );
}
