import styled, { css } from "styled-components";
import { LoadingSpinner } from "./LoadingSpinner";

export default function CardForm({
  children,
  topTitle,
  logoRight,
  logoLeft,
  primaryButtonClick,
  primaryButtonLoading = false,
  secondaryButtonClick,
  logoLeftClick,
  primaryButtonText,
  secondaryButtonText,
  secondaryButtonLoading = false,
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
  primaryButtonLoading?: boolean;
  secondaryButtonLoading?: boolean;
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
        <div className="mb-8 flex w-full flex-row items-center text-center font-inter font-semibold">
          <div
            className="flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg border bg-white-8"
            onClick={logoLeftClick}
          >
            {logoLeft}
          </div>
          <div className="flex-grow text-[15px] leading-[20px]">{topTitle}</div>
          <div className="flex h-8 w-8 items-center justify-center">
            {logoRight}
          </div>
        </div>
      )}
      <div className="flex w-full flex-col">{children}</div>
      <div className="mt-xl flex w-full flex-col gap-md">
        {!secondaryButtonDisabled && (
          <CardFormButton
            text={secondaryButtonText || "Cancel"}
            onClick={secondaryButtonClick}
            kind="secondary"
            loading={secondaryButtonLoading}
          />
        )}
        {!primaryButtonDisabled && (
          <CardFormButton
            text={primaryButtonText || "Submit"}
            onClick={primaryButtonClick}
            loading={primaryButtonLoading}
          />
        )}
      </div>
    </div>
  );
}

const CardFormButton = ({
  text,
  icon,
  kind,
  onClick,
  loading = false,
  height = 44,
}: {
  text?: string;
  icon?: React.ReactNode;
  kind?: "primary" | "secondary";
  onClick?: () => void;
  loading?: boolean;
  height?: number;
}) => {
  return (
    <StyledCardFormButton
      onClick={onClick}
      kind={kind}
      height={height}
      disabled={false}
    >
      {loading ? (
        <LoadingSpinner size={24} />
      ) : (
        <>
          {icon}
          {text}
        </>
      )}
    </StyledCardFormButton>
  );
};

type StyledCardFormButtonProps = {
  kind?: "primary" | "secondary";
  height?: number;
  disabled?: boolean;
};

const StyledCardFormButton = styled.button<StyledCardFormButtonProps>`
  width: 100%;
  height: ${({ height = 44 }) => height}px;
  border-radius: 8px;
  font-weight: 500;
  font-size: 15px;
  line-height: 20px;
  ${({ kind = "primary" }) =>
    kind === "primary"
      ? css`
          background: #fafafa;
          color: #000;
        `
      : css`
          border: 1px solid #2a2a2a;
          background: #1a1a1a;
          color: #fff;
        `}

  &:active {
    transform: scale(0.98);
    background-opacity: 0.5;
  }
`;
