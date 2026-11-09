import { Lock } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Logo } from "../components/Layout";
import { Button, Card, Field, Input } from "../components/ui";
import { ApiError } from "../lib/api";
import { useLogin } from "../lib/hooks";

export default function Login() {
  const [pw, setPw] = useState("");
  const login = useLogin();
  const submit = (e: FormEvent) => { e.preventDefault(); login.mutate(pw); };
  const err = login.error as ApiError | null;
  return (
    <div className="min-h-full grid place-items-center p-4 relative overflow-hidden">
      <div aria-hidden className="absolute -top-40 left-1/2 -translate-x-1/2 size-[560px] rounded-full bg-accent/15 blur-3xl" />
      <div className="relative w-full max-w-sm">
        <div className="flex justify-center mb-6"><Logo /></div>
        <Card className="p-6">
          <h1 className="text-lg font-semibold">Sign in</h1>
          <p className="text-muted mt-1 mb-5">Enter the dashboard password to continue.</p>
          <form onSubmit={submit} className="space-y-4">
            <Field label="Password" error={err?.message}>
              {(id) => <Input id={id} type="password" autoFocus autoComplete="current-password" value={pw} onChange={(e) => setPw(e.target.value)} placeholder="••••••••" />}
            </Field>
            <Button type="submit" variant="primary" className="w-full" loading={login.isPending} disabled={!pw}><Lock className="size-4" />Sign in</Button>
          </form>
        </Card>
        <p className="text-center text-xs text-muted mt-4">Forgot it? Run <code className="font-mono">router-go --reset-password</code></p>
      </div>
    </div>
  );
}
