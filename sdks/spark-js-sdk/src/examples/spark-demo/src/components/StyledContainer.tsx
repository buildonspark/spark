import styled from "styled-components";

export default function StyledContainer({
  children,
  isPressable = false,

  className,
}: {
  children: React.ReactNode;
  isPressable?: boolean;
  className?: string;
}) {
  return isPressable ? (
    <ButtonContainer className={className}>{children}</ButtonContainer>
  ) : (
    <DivContainer className={className}>{children}</DivContainer>
  );
}

const DivContainer = styled.div`
  background: linear-gradient(
    180deg,
    rgba(20, 26, 34, 0.5) 0%,
    rgba(16, 21, 16, 0.5) 100%
  );

  border: 1px solid rgba(249, 249, 249, 0.1);
  border-radius: 24px;
  backdrop-filter: blur(20px);
  box-shadow: 0px 4px 24px rgba(0, 0, 0, 0.25);
`;

const ButtonContainer = styled.button`
  width: 100%;
  background: linear-gradient(
    180deg,
    rgba(20, 26, 34, 0.5) 0%,
    rgba(16, 21, 28, 0.5) 100%
  );

  border: 1px solid rgba(249, 249, 249, 0.1);
  border-radius: 24px;
  backdrop-filter: blur(20px);
  box-shadow: 0px 4px 24px rgba(0, 0, 0, 0.25);
`;
