import clsx from "clsx";
import { OTPInput, SlotProps } from "input-otp";
import styled, { keyframes } from "styled-components";

interface VerificationCodeProps {
  value: string | undefined;
  onChange: (value: string) => void;
  onSubmit: () => void;
}

export default function VerificationCode({
  value,
  onChange,
  onSubmit,
}: VerificationCodeProps) {
  return (
    <div className="flex flex-col gap-2">
      <OTPInput
        maxLength={6}
        containerClassName="group flex items-center has-[:disabled]:opacity-30"
        render={({ slots }) => (
          <>
            <div className="flex gap-2">
              {slots.slice(0, 3).map((slot, idx) => (
                <Slot key={idx} {...slot} />
              ))}
            </div>
            <div className="mx-2">-</div>
            <div className="flex gap-2">
              {slots.slice(3).map((slot, idx) => (
                <Slot key={idx} {...slot} />
              ))}
            </div>
          </>
        )}
        onChange={onChange}
        value={value}
        onSubmit={onSubmit}
        inputMode="numeric"
        pattern="\d*"
        autoFocus
      />
    </div>
  );
}

function Slot(props: SlotProps) {
  return (
    <div
      className={clsx(
        "relative flex h-14 w-14 items-center justify-center rounded-md border-2 border-[#f9f9f914] text-sm transition-all",
        props.isActive && "z-10 !border-[#f9f9f9]",
      )}
    >
      <div>
        {props.char ?? props.placeholderChar}
        {props.hasFakeCaret && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
            <CursorBlink />
          </div>
        )}
      </div>
    </div>
  );
}

const cursorBlink = keyframes`
  0%, 15% {
    opacity: 0;
  }
  35%, 65% {
    opacity: 1;
  }
  85%, 100% {
    opacity: 0;
  }
`;

const CursorBlink = styled.div`
  width: 1px;
  height: 16px;
  background: white;
  animation: ${cursorBlink} 1s ease-in-out infinite;
`;
