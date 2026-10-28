'use client';

import Particles, { initParticlesEngine } from '@tsparticles/react';
import { useEffect, useMemo, useState } from 'react';
import { loadSlim } from '@tsparticles/slim';
import { AnimatePresence, motion } from 'motion/react';

const ParticlesComponent = ({ className }: { className: string }) => {
    const [init, setInit] = useState(false);
    const [compact, setCompact] = useState(false);

    useEffect(() => {
        const media = window.matchMedia('(max-width: 767px), (prefers-reduced-motion: reduce)');
        const update = () => setCompact(media.matches);
        update();
        media.addEventListener('change', update);
        return () => media.removeEventListener('change', update);
    }, []);

    useEffect(() => {
        initParticlesEngine(async (engine) => {
            await loadSlim(engine);
        }).then(() => {
            setInit(true);
        });
    }, []);

    const baseColor = '';
    const foregroundColor = '#FFFFFF';

    const options = useMemo(
        () => ({
            background: {
                color: {
                    value: baseColor
                }
            },
            fpsLimit: compact ? 30 : 120,
            interactivity: {
                events: {
                    onClick: {
                        enable: true,
                        mode: 'repulse'
                    },
                    onHover: {
                        enable: true,
                        mode: 'grab'
                    }
                },
                modes: {
                    repulse: {
                        distance: 200,
                        duration: 0.5
                    },
                    grab: {
                        distance: 150,
                        line_linked: {
                            opacity: 20
                        }
                    }
                }
            },
            particles: {
                color: {
                    value: foregroundColor
                },
                links: {
                    color: foregroundColor,
                    enable: false
                },
                move: {
                    direction: 'none' as const,
                    enable: true,
                    outModes: {
                        default: 'bounce' as const
                    },
                    random: true,
                    speed: 1,
                    straight: false
                },
                number: {
                    density: {
                        enable: true
                    },
                    value: compact ? 45 : 150
                },
                opacity: {
                    value: 1.0
                },
                shape: {
                    type: 'circle'
                },
                size: {
                    value: { min: 1, max: 3 }
                }
            },
            detectRetina: true
        }),
        [compact]
    );

    return init ? (
        <AnimatePresence>
            <motion.div key='particles' initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ type: 'spring', duration: 2 }}>
                <Particles className={className} options={options} />
            </motion.div>
        </AnimatePresence>
    ) : null;
};

export default ParticlesComponent;
