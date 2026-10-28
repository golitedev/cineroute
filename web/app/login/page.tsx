'use client';

import { useT } from '@/lib/i18n';
import { useEffect, useState, type FormEvent } from 'react';
import { LoaderCircle } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { getMe, login, loginDestination, formatApiError } from '@/lib/cineroute-api';

export default function Login() {
    const t = useT();
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<unknown>(null);

    useEffect(() => {
        let active = true;
        void getMe()
            .then(() => {
                if (active) window.location.replace(loginDestination());
            })
            .catch(() => {});
        return () => {
            active = false;
        };
    }, []);

    const submit = async (event: FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        if (submitting) return;
        setSubmitting(true);
        setError(null);
        try {
            await login(username, password);
            window.location.replace(loginDestination());
        } catch (error) {
            setError(error);
            setSubmitting(false);
        }
    };

    return (
        <main className='login-main min-h-screen flex flex-col items-center justify-center gap-6 px-6 py-12 relative z-[2]'>
            <div className='flex flex-col'>
                <h1 className='flex justify-center gap-3'>
                    <img src='/favicon.svg' alt='' className='h-10 w-10' />
                    <img src='/logo.svg' alt='CineRoute' className='h-10 w-auto max-w-[220px]' />
                </h1>
                <p className='text-md text-center font-medium text-muted-foreground'>{t('tagline')}</p>
            </div>
            <Card className='w-full max-w-sm'>
                <CardContent className='p-6'>
                    <form onSubmit={submit} className='space-y-4' aria-label={t('signIn')} aria-busy={submitting}>
                        <div className='space-y-2'>
                            <Label htmlFor='username'>{t('username')}</Label>
                            <Input
                                id='username'
                                name='username'
                                autoComplete='username'
                                required
                                disabled={submitting}
                                value={username}
                                onChange={(event) => setUsername(event.target.value)}
                            />
                        </div>
                        <div className='space-y-2'>
                            <Label htmlFor='password'>{t('password')}</Label>
                            <Input
                                id='password'
                                name='password'
                                type='password'
                                autoComplete='current-password'
                                required
                                disabled={submitting}
                                value={password}
                                onChange={(event) => setPassword(event.target.value)}
                            />
                        </div>
                        <Button type='submit' className='w-full' disabled={submitting}>
                            {submitting && <LoaderCircle className='size-4 animate-spin' aria-hidden='true' />}
                            {submitting ? t('signingIn') : t('signIn')}
                        </Button>
                        {error != null && (
                            <p role='alert' className='text-sm text-destructive break-words'>
                                {formatApiError(error, t)}
                            </p>
                        )}
                    </form>
                </CardContent>
            </Card>
        </main>
    );
}
