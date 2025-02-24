import Button from "./Button";

export default function CardForm({
  children,
  topTitle,
  logoRight,
  logoLeft,
  primaryButtonClick,
  secondaryButtonClick,
  logoLeftClick,
  primaryButtonText,
  secondaryButtonText,
  headerDisabled = false,
  primaryButtonDisabled = false,
  secondaryButtonDisabled = true,
}: {
  children: React.ReactNode;
  topTitle: string;
  logoRight?: React.ReactNode;
  logoLeft?: React.ReactNode;
  primaryButtonDisabled?: boolean;
  secondaryButtonDisabled?: boolean;
  primaryButtonText?: string;
  secondaryButtonText?: string;
  headerDisabled?: boolean;
  primaryButtonClick?: () => void;
  secondaryButtonClick?: () => void;
  logoLeftClick?: () => void;
}) {
  return (
    <div className="mt-2 flex w-full flex-col items-center justify-between">
      {!headerDisabled && (
        <div className="mb-8 flex w-full flex-row items-center text-center font-decimal text-[15px]">
          <div
            className="ml-6 h-6 w-6 cursor-pointer outline-none"
            onClick={logoLeftClick}
          >
            {logoLeft}
          </div>
          <div className="flex-grow">{topTitle}</div>
          <div className="mr-6 flex h-8 w-8 items-center justify-center">
            {logoRight}
          </div>
        </div>
      )}
      <div className="flex w-full flex-col">{children}</div>
      <div className="mb-4 w-full">
        {!secondaryButtonDisabled && (
          <Button
            text={secondaryButtonText || "Cancel"}
            onClick={secondaryButtonClick}
            kind="secondary"
          />
        )}
        {!primaryButtonDisabled && (
          <Button
            text={primaryButtonText || "Submit"}
            onClick={primaryButtonClick}
          />
        )}
      </div>
    </div>
  );
}
