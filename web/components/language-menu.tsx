'use client';

import { Languages } from 'lucide-react';
import { Button } from './ui/button';
import { DropdownMenu, DropdownMenuContent, DropdownMenuCheckboxItem, DropdownMenuTrigger } from './ui/dropdown-menu';
import { languages, useI18n, type Language } from '@/lib/i18n';

export default function LanguageMenu() {
    const { language, setLanguage, t } = useI18n();
    return (
        <div className='language-menu absolute top-4 right-4 z-30'>
            <DropdownMenu>
                <DropdownMenuTrigger asChild>
                    <Button variant='ghost' size='sm' aria-label={t('language')} className='h-10 gap-2 px-2'>
                        <Languages className='size-5' aria-hidden='true' />
                        {language.toUpperCase()}
                    </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align='end'>
                    {Object.entries(languages).map(([code, name]) => (
                        <DropdownMenuCheckboxItem
                            key={code}
                            lang={code}
                            checked={language === code}
                            onSelect={() => setLanguage(code as Language)}
                            className='gap-2'
                        >
                            {name}
                        </DropdownMenuCheckboxItem>
                    ))}
                </DropdownMenuContent>
            </DropdownMenu>
        </div>
    );
}
