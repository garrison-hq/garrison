'use client';

// M5.2 — useStickyBottom (plan §1.7).
//
// Tracks scrollTop vs scrollHeight - clientHeight on the supplied ref
// and returns:
//   - isStuck: true when within 40px of the bottom (auto-scroll on
//              new content)
//   - scrollToBottom(): re-engage stickiness manually (e.g. after
//                       operator clicks the "↓ N new" pill)

import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';

const STUCK_THRESHOLD_PX = 40;

export interface UseStickyBottomResult {
  isStuck: boolean;
  scrollToBottom: () => void;
}

export function useStickyBottom(ref: RefObject<HTMLElement | null>): UseStickyBottomResult {
  const [isStuck, setIsStuck] = useState(true);
  const stuckRef = useRef(true);

  const scrollToBottom = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight });
    stuckRef.current = true;
    setIsStuck(true);
  }, [ref]);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const onScroll = () => {
      const distance = el.scrollHeight - el.scrollTop - el.clientHeight;
      const next = distance < STUCK_THRESHOLD_PX;
      if (stuckRef.current !== next) {
        stuckRef.current = next;
        setIsStuck(next);
      }
    };
    el.addEventListener('scroll', onScroll, { passive: true });
    return () => {
      el.removeEventListener('scroll', onScroll);
    };
  }, [ref]);

  return { isStuck, scrollToBottom };
}
