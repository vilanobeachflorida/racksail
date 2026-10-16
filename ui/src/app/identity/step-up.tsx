import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  lazy,
  type ReactNode,
  Suspense,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useIntl } from "react-intl";
import { toast } from "@/design/components/ui/toaster";
import { ApiError } from "@/lib/api/client";
import { messages } from "../messages";

const StepUpDialog = lazy(() => import("./step-up-dialog"));

interface StepUpValue {
  /**
   * Runs an action; when the panel asks the person to confirm it's them
   * first, asks them, then runs it again.
   */
  guard: <T>(run: () => Promise<T>) => Promise<T>;
  /** Asks the person to confirm it's them; true once they did. */
  confirm: () => Promise<boolean>;
}

const StepUpContext = createContext<StepUpValue>({
  guard: (run) => run(),
  confirm: async () => false,
});

/** Whether an error asks the person to confirm it's them first. */
export function needsStepUp(error: unknown): boolean {
  return error instanceof ApiError && error.code === "auth.step_up_required";
}

// Errors a guard already asked about, so nothing asks again when the person
// chose not to confirm.
const asked = new WeakSet<object>();

/**
 * Lets any part of the panel ask the person to confirm it's them, with
 * their password, an authenticator code or a passkey, before a sensitive
 * action goes ahead. A change that fails for want of it outside a guard
 * asks too, and the person tries again once they've confirmed.
 */
export function StepUpProvider({ children }: { children: ReactNode }) {
  const intl = useIntl();
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const waiting = useRef<{ promise: Promise<boolean>; resolve: (ok: boolean) => void } | null>(
    null,
  );

  const confirm = useCallback(() => {
    if (waiting.current) {
      return waiting.current.promise;
    }
    let resolve: (ok: boolean) => void = () => {};
    const promise = new Promise<boolean>((r) => {
      resolve = r;
    });
    waiting.current = { promise, resolve };
    setOpen(true);
    return promise;
  }, []);

  const done = useCallback((ok: boolean) => {
    const current = waiting.current;
    waiting.current = null;
    setOpen(false);
    current?.resolve(ok);
  }, []);

  const guard = useCallback(
    async <T,>(run: () => Promise<T>): Promise<T> => {
      try {
        return await run();
      } catch (error) {
        if (!needsStepUp(error)) {
          throw error;
        }
        asked.add(error as object);
        if (!(await confirm())) {
          throw error;
        }
        return run();
      }
    },
    [confirm],
  );

  useEffect(
    () =>
      client.getMutationCache().subscribe((event) => {
        if (event.type !== "updated" || event.action.type !== "error") {
          return;
        }
        const error = event.action.error;
        if (needsStepUp(error) && !asked.has(error as object)) {
          asked.add(error as object);
          void confirm().then((ok) => {
            if (ok) {
              toast(intl.formatMessage(messages.stepUpTryAgain));
            }
          });
        }
      }),
    [client, confirm, intl],
  );

  const value = useMemo(() => ({ guard, confirm }), [guard, confirm]);
  return (
    <StepUpContext value={value}>
      {children}
      {open ? (
        <Suspense fallback={null}>
          <StepUpDialog onDone={done} />
        </Suspense>
      ) : null}
    </StepUpContext>
  );
}

/** Asks the person to confirm it's them around sensitive actions. */
export function useStepUp(): StepUpValue {
  return useContext(StepUpContext);
}
