'use client';
import React, { useEffect, useRef, useState } from 'react';
import StatusBar from './status-bar';
import { cn } from '@/lib/utils';

const StatusBarContainer = () => {
    const containerRef = useRef<HTMLDivElement>(null);
    const [isMounted, setIsMounted] = useState(false);

    useEffect(() => {
        setIsMounted(true);
    }, []);

    useEffect(() => {
        const element = containerRef.current;
        if (!element) return;
        const update = () => {
            const controls = element.querySelectorAll('button');
            const visible = Array.from(controls).some((button) => button.getBoundingClientRect().height > 0);
            document.documentElement.style.setProperty('--status-bar-space', visible ? `${element.getBoundingClientRect().height}px` : '0px');
        };
        const observer = new ResizeObserver(update);
        observer.observe(element);
        const mutations = new MutationObserver(update);
        mutations.observe(element, { childList: true, subtree: true });
        update();
        return () => {
            observer.disconnect();
            mutations.disconnect();
            document.documentElement.style.removeProperty('--status-bar-space');
        };
    }, [isMounted]);

    if (!isMounted) return null;
    return (
        <div
            ref={containerRef}
            className={cn(
                'status-bar-container px-4 pb-4 pt-6 overflow-hidden mx-auto w-full fixed bottom-0 flex min-h-[156px] z-[20] justify-center bg-gradient-to-b from-transparent pointer-events-none to-primary/5'
            )}
        >
            <div className='container relative flex'>
                <StatusBar />
            </div>
        </div>
    );
};

export default StatusBarContainer;
