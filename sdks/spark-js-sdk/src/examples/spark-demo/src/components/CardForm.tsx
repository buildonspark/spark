import Button from "./Button";

export default function CardForm({
  children,
  topTitle,
  logoRight,
  logoLeft,
  onSubmit,
  logoLeftClick,
  submitButtonText,
  submitDisabled,
}: {
  children: React.ReactNode;
  topTitle: string;
  logoRight?: React.ReactNode;
  logoLeft?: React.ReactNode;
  submitButtonText?: string;
  onSubmit: () => void;
  logoLeftClick?: () => void;
  submitDisabled?: boolean;
}) {
  return (
    <div className=" flex w-full flex-col justify-between items-center">
      <div className="flex w-full flex-row text-center font-decimal text-[15px]">
        <div className="w-6 h-6 ml-6" onClick={logoLeftClick}>
          {logoLeft}
        </div>
        <div className="flex-grow">{topTitle}</div>
        <div className="w-6 h-6 mr-6">{logoRight}</div>
      </div>
      <div className="flex w-full flex-col p-6">{children}</div>
      {!submitDisabled && (
        <div className="fixed bottom-10 w-full max-w-[400px] p-6">
          <Button
            text={submitButtonText || "Submit"}
            onClick={() => onSubmit()}
          />
        </div>
      )}
    </div>
  );
}
