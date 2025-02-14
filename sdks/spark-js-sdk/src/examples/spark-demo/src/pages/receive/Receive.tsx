import Networks from "../../components/Networks";

export default function Receive() {
  return (
    <div>
      <div className="text-center font-decimal text-[15px]">
        Receive money via
      </div>
      <Networks onSelectNetwork={() => {}} />
    </div>
  );
}
